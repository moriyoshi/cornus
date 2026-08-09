// Cornus fork addition.
//
// Race instrumentation allocates, substantially and variably: the Twrite
// allocation measurement below runs at ~36 KB/op normally and 200-365 KB/op under
// `-race`, which straddles any ceiling tight enough to catch the regression it
// names. A byte ceiling and the race detector measure different things, so the
// measurement skips under `-race` rather than being loosened until it catches
// nothing. The correctness tests beside it still run under `-race`, which is
// where the race detector has something to say.

//go:build !race

package p9

// raceEnabled reports whether the race detector is compiled in.
const raceEnabled = false
