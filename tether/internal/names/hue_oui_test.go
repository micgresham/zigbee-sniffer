package names

import "testing"

func TestMacToExt(t *testing.T) {
	ext, ok := macToExt("00:17:88:01:04:ab:cd:ef-0b-1000")
	if !ok || ext != 0x00178801_04abcdef {
		t.Fatalf("macToExt = %#x ok=%v", ext, ok)
	}
	if _, ok := macToExt("garbage"); ok {
		t.Fatal("expected garbage to fail")
	}
}

func TestVendorAndLabel(t *testing.T) {
	hueExt := uint64(0x00178801_04abcdef)
	if v := vendorForExt(hueExt); v != "Philips Hue" {
		t.Fatalf("vendor = %q, want Philips Hue", v)
	}
	r := New()
	// OUI vendor when no friendly name known
	if got := r.LabelExt(hueExt); got != "Philips Hue" {
		t.Fatalf("LabelExt (OUI) = %q", got)
	}
	// friendly name (from Hue) upgrades over the OUI vendor
	r.SetExt(hueExt, "Living Room Lamp")
	if got := r.LabelExt(hueExt); got != "Living Room Lamp" {
		t.Fatalf("LabelExt (named) = %q", got)
	}
	// unknown OUI → empty
	if got := r.LabelExt(0xAABBCC0000000000); got != "" {
		t.Fatalf("unknown OUI = %q, want empty", got)
	}
}
