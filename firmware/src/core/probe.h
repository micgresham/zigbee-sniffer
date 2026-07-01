// probe.h — active device verification: transmit a MAC data frame addressed to a
// target's short address with the ACK-request bit set; the target's 802.15.4 MAC
// auto-ACKs if it's alive and on-channel. This verifies radio liveness WITHOUT
// joining the network or needing the key (the ACK happens at the MAC layer,
// before any NWK processing). Mainly useful for always-on routers/repeaters.
//
// SAFETY: this TRANSMITS on your live network. Gate it behind an explicit action.
#pragma once

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

// Result callback: target short addr, whether it ACKed, ACK RSSI/LQI.
typedef void (*probe_result_cb_t)(uint16_t target, bool acked, int8_t rssi, uint8_t lqi);

void probe_init(probe_result_cb_t cb);

// Transmit a probe to `target` on PAN `pan_id`. The result arrives via the cb.
void probe_send(uint16_t target, uint16_t pan_id);

#ifdef __cplusplus
}
#endif
