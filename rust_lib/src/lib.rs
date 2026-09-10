#[no_mangle]
pub extern "C" fn sub(a: i64, b: i64) -> i64 {
    let mut x = (a - b) as u64;

    for _ in 0..10_000 {
        x ^= x << 13;
        x ^= x >> 17;
        x ^= x << 5;
    }

    a - b
}
