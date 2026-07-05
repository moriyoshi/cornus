package wire

import "testing"

func TestBlockEnvOpts(t *testing.T) {
	if got := parseCoherenceEnv("subhash, defer subfill"); got != (FeatSubBlockHash | FeatDeferHash | FeatSubBlockFill) {
		t.Fatalf("parseCoherenceEnv = %b", got)
	}
	if got := parseCoherenceEnv("  "); got != 0 {
		t.Fatalf("empty coherence = %b", got)
	}
	for in, want := range map[string]int64{"64k": 64 << 10, "262144": 262144, "1M": 1 << 20, "": 0, "-5": 0, "abc": 0} {
		if got := parseByteSizeEnv(in); got != want {
			t.Fatalf("parseByteSizeEnv(%q) = %d, want %d", in, got, want)
		}
	}

	// End to end: env -> opts -> resolved. subfill implies subhash.
	t.Setenv("CORNUS_BLOCK_COHERENCE", "subfill")
	t.Setenv("CORNUS_BLOCK_READAHEAD", "128k")
	o := resolveBlockOpts(BlockEnvOpts())
	if o.features != (FeatSubBlockFill | FeatSubBlockHash) {
		t.Fatalf("resolved features = %b, want subfill+subhash", o.features)
	}
	if o.readahead != 128<<10 {
		t.Fatalf("readahead = %d, want %d", o.readahead, 128<<10)
	}

	// Unset env -> the classic path (no opts).
	t.Setenv("CORNUS_BLOCK_COHERENCE", "")
	t.Setenv("CORNUS_BLOCK_READAHEAD", "")
	if o := resolveBlockOpts(BlockEnvOpts()); o.features != 0 || o.readahead != 0 {
		t.Fatalf("empty env should be classic, got %+v", o)
	}
}
