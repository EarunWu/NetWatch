use std::sync::atomic::Ordering;

use tauri::{
    AppHandle, Manager, WebviewUrl, WebviewWindow, WebviewWindowBuilder, Window, WindowEvent,
};
use tauri_plugin_dialog::{
    DialogExt, MessageDialogButtons, MessageDialogKind, MessageDialogResult,
};

use crate::{sidecar, state::DesktopState};

pub(crate) const MAIN_WINDOW_LABEL: &str = "main";

pub(crate) fn show_or_create_main(app: &AppHandle) {
    app.state::<DesktopState>()
        .tray_mode
        .store(false, Ordering::Release);
    let window = match app.get_webview_window(MAIN_WINDOW_LABEL) {
        Some(window) => window,
        None => match build_main_window(app) {
            Ok(window) => window,
            Err(error) => {
                show_error(app, format!("无法创建监测窗口：{error}"));
                return;
            }
        },
    };

    if window.is_minimized().unwrap_or(false) {
        let _ = window.unminimize();
    }
    let _ = window.show();
    let _ = window.set_focus();
}

fn build_main_window(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    WebviewWindowBuilder::new(app, MAIN_WINDOW_LABEL, WebviewUrl::App("index.html".into()))
        .title("链路哨兵")
        .inner_size(1380.0, 900.0)
        .min_inner_size(980.0, 680.0)
        .resizable(true)
        .visible(false)
        .center()
        .build()
}

pub(crate) fn handle_window_event(window: &Window, event: &WindowEvent) {
    if window.label() != MAIN_WINDOW_LABEL {
        return;
    }

    if let WindowEvent::CloseRequested { api, .. } = event {
        let app = window.app_handle();
        let state = app.state::<DesktopState>();
        if state.is_exiting() {
            return;
        }

        api.prevent_close();
        prompt_close_choice(app);
    }
}

pub(crate) fn prompt_close_choice(app: &AppHandle) {
    let app = app.clone();
    let state = app.state::<DesktopState>();
    if state.close_dialog_open.swap(true, Ordering::AcqRel) {
        return;
    }

    app.dialog()
        .message("关闭监测页面后，要继续在后台监测，还是退出监测？")
        .title("关闭链路哨兵")
        .kind(MessageDialogKind::Info)
        .buttons(MessageDialogButtons::YesNoCancelCustom(
            "最小化到托盘".into(),
            "退出监测".into(),
            "取消".into(),
        ))
        .show_with_result(move |result| {
            let state = app.state::<DesktopState>();
            state.close_dialog_open.store(false, Ordering::Release);

            match result {
                MessageDialogResult::Yes => destroy_main_window(&app),
                MessageDialogResult::No => sidecar::request_app_exit(app.clone()),
                MessageDialogResult::Custom(label) if label == "最小化到托盘" => {
                    destroy_main_window(&app)
                }
                MessageDialogResult::Custom(label) if label == "退出监测" => {
                    sidecar::request_app_exit(app.clone())
                }
                MessageDialogResult::Cancel
                | MessageDialogResult::Ok
                | MessageDialogResult::Custom(_) => {}
            }
        });
}

pub(crate) fn destroy_main_window(app: &AppHandle) {
    app.state::<DesktopState>()
        .tray_mode
        .store(true, Ordering::Release);
    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
        if window.destroy().is_err() {
            app.state::<DesktopState>()
                .tray_mode
                .store(false, Ordering::Release);
        } else {
            trim_working_set_after_webview_exit();
        }
    }
}

#[cfg(target_os = "windows")]
fn trim_working_set_after_webview_exit() {
    // Destroying WebView2 removes its child processes, but Windows can keep
    // already-unused runtime pages resident in this lightweight tray process.
    // Give WebView2 time to tear down, then ask the OS to reclaim those pages.
    std::thread::spawn(|| {
        std::thread::sleep(std::time::Duration::from_millis(750));
        unsafe {
            use windows_sys::Win32::{
                System::ProcessStatus::EmptyWorkingSet, System::Threading::GetCurrentProcess,
            };
            let _ = EmptyWorkingSet(GetCurrentProcess());
        }
    });
}

#[cfg(not(target_os = "windows"))]
fn trim_working_set_after_webview_exit() {}

pub(crate) fn show_error(app: &AppHandle, message: impl Into<String>) {
    app.dialog()
        .message(message.into())
        .title("链路哨兵")
        .kind(MessageDialogKind::Error)
        .show(|_| {});
}
