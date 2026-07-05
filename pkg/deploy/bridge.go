package deploy

import "io"

// Bridge copies bytes bidirectionally between the caller's client conn and a
// backend-side raw stdio stream (a hijacked Docker exec/attach connection, a
// containerd exec's FIFO pair, a dialed container port, ...). The two
// directions are NOT symmetric:
//
//   - Output (remote -> client) is authoritative. Bridge returns only when this
//     copy finishes, i.e. when the remote closes its side (the process exited
//     and its stdout/stderr are drained). Then both conns are closed.
//   - Input (client -> remote) carries stdin. When it finishes (the client's
//     stdin reaches EOF — a non-interactive `docker exec` sends none, so this
//     happens immediately) the tunnel MUST NOT be torn down, or the output
//     would be truncated before the process's stdout arrives. Instead the
//     remote write side is half-closed (CloseWrite when the conn supports it)
//     so the process sees stdin EOF while its output keeps flowing back.
//
// The output copy's error (if any) is returned.
func Bridge(client, remote io.ReadWriteCloser) error {
	outDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(client, remote) // output: remote -> client (authoritative)
		outDone <- err
	}()
	go func() {
		_, _ = io.Copy(remote, client) // input: client stdin -> remote
		// stdin ended: half-close the remote write side, keep output flowing.
		if cw, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	err := <-outDone
	remote.Close()
	client.Close() // unblocks the input copy if it is still reading stdin
	return err
}
