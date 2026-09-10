#include "calculator.h"

int64_t add(int64_t a, int64_t b) {
    volatile uint64_t x = (uint64_t)(a + b);

    for (int i = 0; i < 10000; i++) {
        x ^= x << 13;
        x ^= x >> 17;
        x ^= x << 5;
    }

    return a + b;
}
