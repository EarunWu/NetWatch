mod desktop_window;
mod sidecar;
mod state;
mod tray;

use tauri::{Manager, RunEvent};

use state::DesktopState;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let app = tauri::Builder::default()
        // The single-instance plugin must be registered before all other plugins.
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            desktop_window::show_or_create_main(app);
        }))
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .manage(DesktopState::default())
        .setup(|app| {
            tray::create(app)?;
            sidecar::start(app.handle().clone());
            Ok(())
        })
        .on_window_event(desktop_window::handle_window_event)
        .build(tauri::generate_context!())
        .expect("failed to build NetWatch desktop application");

    app.run(|app, event| match event {
        RunEvent::ExitRequested { api, .. } => {
            let state = app.state::<DesktopState>();
            if !state.is_exiting() {
                api.prevent_exit();
                // Destroying the last WebView is the low-memory tray path and
                // must not open a second prompt. Cmd+Q with a live window does.
                if !state.tray_mode.load(std::sync::atomic::Ordering::Acquire) {
                    if app
                        .get_webview_window(desktop_window::MAIN_WINDOW_LABEL)
                        .is_some()
                    {
                        desktop_window::prompt_close_choice(app);
                    }
                }
            }
        }
        RunEvent::Exit => sidecar::force_kill(app),
        _ => {}
    });
}
