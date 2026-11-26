package clientprotocol

import "testing"

func TestDecodeServers_NoEntriesYieldsAnEmptyNonNilList(t *testing.T) {
	t.Parallel()

	got, err := decodeServers(nil)

	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("decodeServers(nil) = %#v, %v, want an empty non-nil list: the wire must carry [] and not null", got, err)
	}
}
