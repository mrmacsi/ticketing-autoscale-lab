// Package seedrand derives independent, reproducible random streams from the
// experiment seed. Every consumer gets its own stream (identified by a salt and
// an index) so that concurrency or the order of goroutines never changes the
// decisions made for a given arrival.
package seedrand

import "math/rand/v2"

// Stream salts. Changing these changes every derived decision.
const (
	SaltSchedule uint64 = 0x5c4ed_0001
	SaltBursts   uint64 = 0x5c4ed_0002
	SaltArrival  uint64 = 0x5c4ed_0003
)

// New returns a PCG-backed RNG for (seed, salt, index).
func New(seed int64, salt, index uint64) *rand.Rand {
	hi := splitmix64(uint64(seed) ^ splitmix64(salt))
	lo := splitmix64(hi ^ splitmix64(index+salt))
	return rand.New(rand.NewPCG(hi, lo))
}

func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
