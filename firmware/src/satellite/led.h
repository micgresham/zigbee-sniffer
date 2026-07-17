// led.h — onboard status LED (satellite build only). Color reflects the
// satellite's assigned capture channel so several boards on a desk/carrier are
// visually distinguishable at a glance. See docs/carrier.md.
#pragma once

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void sat_led_init(void);
void sat_led_set_channel(uint8_t channel); // ZB_CHANNEL_MIN..ZB_CHANNEL_MAX

#ifdef __cplusplus
}
#endif
