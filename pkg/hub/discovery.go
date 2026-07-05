package hub

import (
	"fmt"
	"hash/fnv"
)

// SyntheticIP maps a service name to a deterministic address in the 127.0.0.0/8
// loopback range. A caretaker binds it for that service's Reach listener and its
// DNS role resolves the service name to it, so an app dialing the name is funneled
// transparently into the hub. Avoids 127.0.0.1 and a zero last octet. Collisions
// across a pod's handful of imported peers are astronomically unlikely.
func SyntheticIP(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	v := h.Sum32()
	a := byte(v >> 16)
	b := byte(v >> 8)
	c := byte(v) | 1 // never a zero last octet
	if a == 0 && b == 0 && c == 1 {
		c = 3 // never 127.0.0.1
	}
	return fmt.Sprintf("127.%d.%d.%d", a, b, c)
}
