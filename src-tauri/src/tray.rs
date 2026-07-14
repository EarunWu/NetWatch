use tauri::{
    image::Image,
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    App,
};

use crate::{desktop_window, sidecar};

const TRAY_ID: &str = "netwatch-tray";

pub(crate) fn create(app: &App) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, "open", "打开监测面板", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出监测", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &quit])?;

    TrayIconBuilder::with_id(TRAY_ID)
        .icon(make_tray_icon())
        .tooltip("链路哨兵")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id.as_ref() {
            "open" => desktop_window::show_or_create_main(app),
            "quit" => sidecar::request_app_exit(app.clone()),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                desktop_window::show_or_create_main(tray.app_handle());
            }
        })
        .build(app)?;

    Ok(())
}

fn make_tray_icon() -> Image<'static> {
    const SIZE: u32 = 32;
    let mut rgba = vec![0_u8; (SIZE * SIZE * 4) as usize];

    for y in 0..SIZE {
        for x in 0..SIZE {
            let dx = x as i32 - 15;
            let dy = y as i32 - 15;
            let inside = dx * dx + dy * dy <= 14 * 14;
            if !inside {
                continue;
            }

            let offset = ((y * SIZE + x) * 4) as usize;
            rgba[offset] = 31;
            rgba[offset + 1] = 128;
            rgba[offset + 2] = 224;
            rgba[offset + 3] = 255;

            // A small rising signal line remains legible at Windows tray sizes.
            let signal = (x >= 7 && x <= 10 && y >= 19)
                || (x >= 13 && x <= 16 && y >= 14)
                || (x >= 19 && x <= 22 && y >= 9);
            if signal {
                rgba[offset] = 235;
                rgba[offset + 1] = 248;
                rgba[offset + 2] = 255;
            }
        }
    }

    Image::new_owned(rgba, SIZE, SIZE)
}
