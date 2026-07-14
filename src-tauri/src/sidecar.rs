use std::{
    io::{Read, Write},
    net::{SocketAddr, TcpStream},
    sync::{
        atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering},
        Mutex, MutexGuard,
    },
    thread,
    time::{Duration, Instant},
};

use serde_json::Value;
use tauri::{AppHandle, Manager};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

use crate::{desktop_window, state::DesktopState};

const SIDECAR_NAME: &str = "NetWatch.Service";
const LISTEN_ADDRESS: &str = "127.0.0.1:9288";
const HEALTH_ADDRESS: &str = "127.0.0.1:9288";
const PORT_BUSY_PREFIX: &str = "PORT_BUSY: ";
const MAX_RESTARTS: u32 = 5;
const HEALTH_WAIT: Duration = Duration::from_secs(10);
const GRACEFUL_SHUTDOWN_WAIT: Duration = Duration::from_secs(3);
const STABLE_RUN_TIME: Duration = Duration::from_secs(5 * 60);

struct RunningChild {
    generation: u64,
    child: CommandChild,
}

pub(crate) struct SidecarSupervisor {
    child: Mutex<Option<RunningChild>>,
    next_generation: AtomicU64,
    active_generation: AtomicU64,
    ready_seen_generation: AtomicU64,
    healthy_generation: AtomicU64,
    fatal_generation: AtomicU64,
    fatal_message: Mutex<Option<String>>,
    expected_stop: AtomicBool,
    restart_scheduled: AtomicBool,
    consecutive_failures: AtomicU32,
}

impl Default for SidecarSupervisor {
    fn default() -> Self {
        Self {
            child: Mutex::new(None),
            next_generation: AtomicU64::new(0),
            active_generation: AtomicU64::new(0),
            ready_seen_generation: AtomicU64::new(0),
            healthy_generation: AtomicU64::new(0),
            fatal_generation: AtomicU64::new(0),
            fatal_message: Mutex::new(None),
            expected_stop: AtomicBool::new(false),
            restart_scheduled: AtomicBool::new(false),
            consecutive_failures: AtomicU32::new(0),
        }
    }
}

pub(crate) fn start(app: AppHandle) {
    if let Err(error) = spawn_sidecar(&app) {
        let message = error
            .strip_prefix(PORT_BUSY_PREFIX)
            .map(str::to_owned)
            .unwrap_or_else(|| format!("无法启动采集服务：{error}"));
        desktop_window::show_error(&app, message);
    }
}

fn spawn_sidecar(app: &AppHandle) -> Result<(), String> {
    let state = app.state::<DesktopState>();
    if state.is_exiting() || state.sidecar.expected_stop.load(Ordering::Acquire) {
        return Ok(());
    }

    {
        let child = lock(&state.sidecar.child);
        if child.is_some() {
            return Ok(());
        }
    }

    if port_is_in_use() {
        return Err(format!(
            "{PORT_BUSY_PREFIX}本机端口 9288 已被占用。请先退出旧版 NetWatch.Service.exe 或占用该端口的程序，再重新启动链路哨兵。"
        ));
    }

    let command = app
        .shell()
        .sidecar(SIDECAR_NAME)
        .map_err(|error| error.to_string())?
        .args(["--managed", "--listen", LISTEN_ADDRESS]);
    let (receiver, child) = command.spawn().map_err(|error| error.to_string())?;

    let generation = state.sidecar.next_generation.fetch_add(1, Ordering::AcqRel) + 1;
    state
        .sidecar
        .active_generation
        .store(generation, Ordering::Release);
    state
        .sidecar
        .ready_seen_generation
        .store(0, Ordering::Release);
    state.sidecar.healthy_generation.store(0, Ordering::Release);
    state.sidecar.fatal_generation.store(0, Ordering::Release);
    *lock(&state.sidecar.fatal_message) = None;
    *lock(&state.sidecar.child) = Some(RunningChild { generation, child });

    consume_output(app.clone(), generation, receiver);
    wait_until_ready(app.clone(), generation);
    Ok(())
}

fn consume_output(
    app: AppHandle,
    generation: u64,
    mut receiver: tauri::async_runtime::Receiver<CommandEvent>,
) {
    tauri::async_runtime::spawn(async move {
        let mut termination_seen = false;
        while let Some(event) = receiver.recv().await {
            match event {
                CommandEvent::Stdout(bytes) => {
                    let line = String::from_utf8_lossy(&bytes).trim().to_owned();
                    if !line.is_empty() {
                        handle_stdout(&app, generation, &line);
                    }
                }
                CommandEvent::Stderr(bytes) => {
                    let line = String::from_utf8_lossy(&bytes).trim().to_owned();
                    if !line.is_empty() {
                        eprintln!("NetWatch.Service: {line}");
                    }
                }
                CommandEvent::Error(error) => {
                    eprintln!("NetWatch.Service output error: {error}");
                }
                CommandEvent::Terminated(_) => {
                    termination_seen = true;
                    handle_terminated(&app, generation);
                    break;
                }
                _ => {}
            }
        }

        // A closed command-event stream is also terminal. Generation checks make
        // this safe if a Terminated event was already handled.
        if !termination_seen {
            handle_terminated(&app, generation);
        }
    });
}

fn handle_stdout(app: &AppHandle, generation: u64, line: &str) {
    let Ok(value) = serde_json::from_str::<Value>(line) else {
        eprintln!("NetWatch.Service: {line}");
        return;
    };
    let event_type = value.get("type").and_then(Value::as_str).unwrap_or("");
    let state = app.state::<DesktopState>();
    if state.sidecar.active_generation.load(Ordering::Acquire) != generation {
        return;
    }

    match event_type {
        "ready" => state
            .sidecar
            .ready_seen_generation
            .store(generation, Ordering::Release),
        "fatal" => {
            state
                .sidecar
                .fatal_generation
                .store(generation, Ordering::Release);
            let message = value
                .get("message")
                .and_then(Value::as_str)
                .unwrap_or("采集服务发生不可恢复的启动错误")
                .to_owned();
            *lock(&state.sidecar.fatal_message) = Some(message);
        }
        _ => eprintln!("NetWatch.Service: {line}"),
    }
}

fn wait_until_ready(app: AppHandle, generation: u64) {
    thread::spawn(move || {
        let deadline = Instant::now() + HEALTH_WAIT;
        while Instant::now() < deadline {
            let state = app.state::<DesktopState>();
            if state.is_exiting()
                || state.sidecar.active_generation.load(Ordering::Acquire) != generation
            {
                return;
            }

            let ready_seen =
                state.sidecar.ready_seen_generation.load(Ordering::Acquire) == generation;
            if ready_seen && health_is_ready() {
                state
                    .sidecar
                    .healthy_generation
                    .store(generation, Ordering::Release);
                if state.initial_window_pending.swap(false, Ordering::AcqRel) {
                    desktop_window::show_or_create_main(&app);
                }
                reset_failures_after_stable_run(app.clone(), generation);
                return;
            }
            thread::sleep(Duration::from_millis(100));
        }

        eprintln!("NetWatch.Service did not become healthy within 10 seconds");
    });
}

fn reset_failures_after_stable_run(app: AppHandle, generation: u64) {
    thread::spawn(move || {
        thread::sleep(STABLE_RUN_TIME);
        let state = app.state::<DesktopState>();
        if !state.is_exiting()
            && state.sidecar.active_generation.load(Ordering::Acquire) == generation
            && state.sidecar.healthy_generation.load(Ordering::Acquire) == generation
            && health_is_ready()
        {
            state
                .sidecar
                .consecutive_failures
                .store(0, Ordering::Release);
        }
    });
}

fn health_is_ready() -> bool {
    let Ok(address) = HEALTH_ADDRESS.parse::<SocketAddr>() else {
        return false;
    };
    let Ok(mut stream) = TcpStream::connect_timeout(&address, Duration::from_millis(300)) else {
        return false;
    };
    let _ = stream.set_read_timeout(Some(Duration::from_millis(500)));
    let _ = stream.set_write_timeout(Some(Duration::from_millis(500)));

    let request = b"GET /api/health HTTP/1.1\r\nHost: 127.0.0.1:9288\r\nAccept: application/json\r\nConnection: close\r\n\r\n";
    if stream.write_all(request).is_err() {
        return false;
    }

    let mut response = Vec::with_capacity(1024);
    if stream.take(16 * 1024).read_to_end(&mut response).is_err() {
        return false;
    }
    let response = String::from_utf8_lossy(&response);
    response.starts_with("HTTP/1.1 200") && response.contains("\"status\":\"ok\"")
}

fn port_is_in_use() -> bool {
    let Ok(address) = HEALTH_ADDRESS.parse::<SocketAddr>() else {
        return false;
    };
    TcpStream::connect_timeout(&address, Duration::from_millis(200)).is_ok()
}

fn handle_terminated(app: &AppHandle, generation: u64) {
    let state = app.state::<DesktopState>();
    let removed = {
        let mut child = lock(&state.sidecar.child);
        if child.as_ref().map(|item| item.generation) == Some(generation) {
            child.take();
            true
        } else {
            false
        }
    };
    if !removed {
        return;
    }

    state.sidecar.active_generation.store(0, Ordering::Release);
    state.sidecar.healthy_generation.store(0, Ordering::Release);

    if state.is_exiting() || state.sidecar.expected_stop.load(Ordering::Acquire) {
        return;
    }

    if state.sidecar.fatal_generation.load(Ordering::Acquire) == generation {
        let message = lock(&state.sidecar.fatal_message)
            .clone()
            .unwrap_or_else(|| "采集服务发生不可恢复的启动错误".to_owned());
        desktop_window::show_error(app, message);
        return;
    }

    schedule_restart(app.clone());
}

fn schedule_restart(app: AppHandle) {
    let state = app.state::<DesktopState>();
    let attempt = state
        .sidecar
        .consecutive_failures
        .fetch_add(1, Ordering::AcqRel)
        + 1;
    if attempt > MAX_RESTARTS {
        desktop_window::show_error(
            &app,
            "采集服务连续异常退出，已停止自动重启。请退出链路哨兵后查看 netwatch.log。",
        );
        return;
    }
    if state.sidecar.restart_scheduled.swap(true, Ordering::AcqRel) {
        return;
    }

    let delay = match attempt {
        1 => 1,
        2 => 2,
        3 => 5,
        4 => 10,
        _ => 30,
    };
    thread::spawn(move || {
        thread::sleep(Duration::from_secs(delay));
        let state = app.state::<DesktopState>();
        state
            .sidecar
            .restart_scheduled
            .store(false, Ordering::Release);
        if state.is_exiting() || state.sidecar.expected_stop.load(Ordering::Acquire) {
            return;
        }
        if let Err(error) = spawn_sidecar(&app) {
            eprintln!("could not restart NetWatch.Service: {error}");
            if error.starts_with(PORT_BUSY_PREFIX) {
                desktop_window::show_error(&app, error.trim_start_matches(PORT_BUSY_PREFIX));
                return;
            }
            schedule_restart(app.clone());
        }
    });
}

pub(crate) fn request_app_exit(app: AppHandle) {
    let state = app.state::<DesktopState>();
    if state.explicit_exit.swap(true, Ordering::AcqRel) {
        return;
    }
    state.sidecar.expected_stop.store(true, Ordering::Release);
    state
        .sidecar
        .restart_scheduled
        .store(false, Ordering::Release);

    {
        let mut child = lock(&state.sidecar.child);
        if let Some(running) = child.as_mut() {
            let _ = running.child.write(b"shutdown\n");
        }
    }

    thread::spawn(move || {
        let deadline = Instant::now() + GRACEFUL_SHUTDOWN_WAIT;
        while Instant::now() < deadline {
            if lock(&app.state::<DesktopState>().sidecar.child).is_none() {
                app.exit(0);
                return;
            }
            thread::sleep(Duration::from_millis(50));
        }

        force_kill(&app);
        app.exit(0);
    });
}

pub(crate) fn force_kill(app: &AppHandle) {
    let state = app.state::<DesktopState>();
    state.sidecar.expected_stop.store(true, Ordering::Release);
    let running = lock(&state.sidecar.child).take();
    if let Some(running) = running {
        let _ = running.child.kill();
    }
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}
