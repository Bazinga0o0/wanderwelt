//! Prozesszähler (CPU-Zeit, Spitzen-Arbeitsspeicher) für Messläufe.
//!
//! Unter Windows über zwei Systemaufrufe; sonst 0. Das ist die einzige
//! Stelle mit `unsafe`: Die Aufrufe lesen nur Zähler des eigenen Prozesses in
//! eigene, korrekt dimensionierte Strukturen (`#[repr(C)]`, Größe wird
//! mitgegeben) und haben keine weiteren Vorbedingungen.

/// CPU-Zeit des Prozesses und Spitzen-Arbeitsspeicher.
#[derive(Debug, Clone, Copy, Default)]
pub struct Usage {
    pub cpu_user_ms: u64,
    pub cpu_kernel_ms: u64,
    pub peak_working_set_bytes: u64,
}

#[cfg(windows)]
#[allow(unsafe_code)]
pub fn usage() -> Usage {
    #[repr(C)]
    #[derive(Default, Clone, Copy)]
    struct FileTime {
        lo: u32,
        hi: u32,
    }
    #[repr(C)]
    #[derive(Default)]
    struct Pmc {
        cb: u32,
        page_fault_count: u32,
        peak_working_set_size: usize,
        working_set_size: usize,
        quota_peak_paged_pool_usage: usize,
        quota_paged_pool_usage: usize,
        quota_peak_non_paged_pool_usage: usize,
        quota_non_paged_pool_usage: usize,
        pagefile_usage: usize,
        peak_pagefile_usage: usize,
    }
    unsafe extern "system" {
        fn GetCurrentProcess() -> isize;
        fn GetProcessTimes(
            h: isize,
            creation: *mut FileTime,
            exit: *mut FileTime,
            kernel: *mut FileTime,
            user: *mut FileTime,
        ) -> i32;
        fn K32GetProcessMemoryInfo(h: isize, p: *mut Pmc, cb: u32) -> i32;
    }
    let ms = |t: FileTime| ((u64::from(t.hi) << 32) | u64::from(t.lo)) / 10_000;
    let mut u = Usage::default();
    // SAFETY: Alle Zeiger zeigen auf lebende, passend große lokale Strukturen;
    // das Pseudo-Handle des eigenen Prozesses ist immer gültig.
    unsafe {
        let h = GetCurrentProcess();
        let (mut c, mut e, mut k, mut us) = (FileTime::default(), FileTime::default(), FileTime::default(), FileTime::default());
        if GetProcessTimes(h, &mut c, &mut e, &mut k, &mut us) != 0 {
            u.cpu_user_ms = ms(us);
            u.cpu_kernel_ms = ms(k);
        }
        let mut p = Pmc { cb: size_of::<Pmc>() as u32, ..Pmc::default() };
        if K32GetProcessMemoryInfo(h, &mut p, size_of::<Pmc>() as u32) != 0 {
            u.peak_working_set_bytes = p.peak_working_set_size as u64;
        }
    }
    u
}

#[cfg(not(windows))]
pub fn usage() -> Usage {
    Usage::default()
}
