use std::sync::atomic::{AtomicBool, Ordering};

use crate::sidecar::SidecarSupervisor;

pub(crate) struct DesktopState {
    pub(crate) explicit_exit: AtomicBool,
    pub(crate) close_dialog_open: AtomicBool,
    pub(crate) initial_window_pending: AtomicBool,
    pub(crate) tray_mode: AtomicBool,
    pub(crate) sidecar: SidecarSupervisor,
}

impl Default for DesktopState {
    fn default() -> Self {
        Self {
            explicit_exit: AtomicBool::new(false),
            close_dialog_open: AtomicBool::new(false),
            initial_window_pending: AtomicBool::new(true),
            tray_mode: AtomicBool::new(false),
            sidecar: SidecarSupervisor::default(),
        }
    }
}

impl DesktopState {
    pub(crate) fn is_exiting(&self) -> bool {
        self.explicit_exit.load(Ordering::Acquire)
    }
}
