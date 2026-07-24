package handlers

import (
	"testing"
	"time"

	"minilinux-server/internal/arp"
)

// Verifie le comportement de macMatchesPeer face a une entree ARP reelle du
// systeme : un MAC revendique qui ne correspond pas au pair doit etre rejete,
// et l'absence d'entree ARP ne doit pas casser les installs legitimes.
func TestMacMatchesPeer(t *testing.T) {
	h := &Handlers{arpCache: arp.NewCache(30 * time.Second)}

	// 172.16.2.99 -> 52:54:00:fd:18:a8 dans /proc/net/arp
	const ip = "172.16.2.99"
	const realMAC = "52:54:00:FD:18:A8"

	if got := h.arpCache.Lookup(ip); got == "" || got == "UNKNOWN" {
		t.Skipf("pas d'entree ARP pour %s dans cet environnement", ip)
	}

	if ok, verified := h.macMatchesPeer(realMAC, ip); !ok || !verified {
		t.Errorf("MAC correct rejete: ok=%v verified=%v", ok, verified)
	}
	if ok, verified := h.macMatchesPeer("AA:BB:CC:DD:EE:FF", ip); ok || !verified {
		t.Errorf("MAC usurpe accepte: ok=%v verified=%v", ok, verified)
	}
	// Casse differente : doit rester accepte (comparaison insensible).
	if ok, _ := h.macMatchesPeer("52:54:00:fd:18:a8", ip); !ok {
		t.Error("comparaison sensible a la casse")
	}
	// IP sans entree ARP : indecidable -> accepte mais non verifie.
	if ok, verified := h.macMatchesPeer(realMAC, "203.0.113.199"); !ok || verified {
		t.Errorf("IP sans ARP mal geree: ok=%v verified=%v", ok, verified)
	}
}
