// probe.c — see probe.h.
#include "probe.h"
#include "proto.h"
#include <string.h>
#include "esp_ieee802154.h"
#include "esp_log.h"

static const char *TAG = "probe";
static probe_result_cb_t s_cb;
static volatile uint16_t s_target;
static uint8_t s_seq = 0x80;

void probe_init(probe_result_cb_t cb) { s_cb = cb; }

void probe_send(uint16_t target, uint16_t pan_id)
{
    s_target = target;
    // The radio must be awake to transmit. If capture was stopped it's asleep,
    // so wake it into RX first (also lets us hear the ACK).
    esp_ieee802154_set_rx_when_idle(true);
    esp_ieee802154_receive();
    // Minimal 802.15.4 MAC data frame addressed to the target, ACK requested.
    // FCF bits: type=Data(1), ack-request(bit5), PAN-ID-compression(bit6),
    //           dest addr short(bits10-11=2), src addr short(bits14-15=2).
    uint16_t fcf = 0x0001 | (1u << 5) | (1u << 6) | (2u << 10) | (2u << 14);

    uint8_t f[1 + 16];
    uint8_t n = 1;                 // f[0] is the PHY length, filled last
    f[n++] = fcf & 0xFF;
    f[n++] = fcf >> 8;
    f[n++] = s_seq++;              // sequence number
    f[n++] = pan_id & 0xFF;        // dest PAN
    f[n++] = pan_id >> 8;
    f[n++] = target & 0xFF;        // dest short address
    f[n++] = target >> 8;
    f[n++] = 0xFE;                 // src short address 0xFFFE (no payload follows)
    f[n++] = 0xFF;
    // PHY length = MPDU bytes + 2 (the hardware appends/derives the FCS).
    f[0] = (n - 1) + 2;
    esp_ieee802154_transmit(f, false);
}

// TX succeeded; for an ACK-requested frame this means the target ACKed.
void esp_ieee802154_transmit_done(const uint8_t *frame, const uint8_t *ack,
                                  esp_ieee802154_frame_info_t *ack_frame_info)
{
    (void)frame; (void)ack;
    if (s_cb) {
        int8_t rssi = ack_frame_info ? ack_frame_info->rssi : 0;
        uint8_t lqi = ack_frame_info ? ack_frame_info->lqi : 0;
        s_cb(s_target, true, rssi, lqi);
    }
}

void esp_ieee802154_transmit_failed(const uint8_t *frame, esp_ieee802154_tx_error_t error)
{
    (void)frame;
    ESP_LOGD(TAG, "probe to 0x%04x failed (err %d)", s_target, error);
    if (s_cb) s_cb(s_target, false, 0, 0);
}
