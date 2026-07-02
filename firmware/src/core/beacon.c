// beacon.c — see beacon.h.
#include "beacon.h"
#include "esp_ieee802154.h"

static uint8_t s_seq = 0xC0;

void beacon_request_send(void)
{
    // The radio must be awake to transmit. If capture was stopped it's asleep,
    // so wake it into RX first (this also lets us hear the beacon replies).
    esp_ieee802154_set_rx_when_idle(true);
    esp_ieee802154_receive();

    // 802.15.4 MAC command "Beacon Request" (IEEE 802.15.4-2015 §5.3.7):
    //   FCF = type=MAC command(3), no security, dst PAN + dst short addr present,
    //         src addressing = none, PAN-id compression = 0.
    //   FCF bits: type(0-2)=3, dst-addr-mode(10-11)=short(2) -> 0x0803.
    //   MPDU: FCF(2) seq(1) dstPAN=0xFFFF(2) dstAddr=0xFFFF(2) cmdId=0x07(1)
    uint16_t fcf = 0x0803;

    uint8_t f[1 + 8];
    uint8_t n = 1;                 // f[0] is the PHY length, filled last
    f[n++] = fcf & 0xFF;
    f[n++] = fcf >> 8;
    f[n++] = s_seq++;              // sequence number
    f[n++] = 0xFF;                 // dest PAN = broadcast
    f[n++] = 0xFF;
    f[n++] = 0xFF;                 // dest short address = broadcast
    f[n++] = 0xFF;
    f[n++] = 0x07;                 // MAC command frame identifier = Beacon Request
    // PHY length = MPDU bytes + 2 (the hardware derives/appends the FCS).
    f[0] = (n - 1) + 2;
    esp_ieee802154_transmit(f, false);
}
