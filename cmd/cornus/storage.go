package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// StorageCmd groups server storage administration. Today it exposes a
// non-destructive usage report; reclamation stays server-side (POST /.cornus/v1/gc,
// the periodic scheduler).
type StorageCmd struct {
	Usage StorageUsageCmd `kong:"cmd,help='Report current registry storage consumption (blob count/bytes, block-cache footprint).'"`
}

// StorageUsageCmd prints the server's non-destructive disk-usage report
// (GET /.cornus/v1/storage).
type StorageUsageCmd struct {
	Server string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL (http(s):// or ws(s)://). Falls back to the selected connection profile (see cornus config).'"`
	Format string `kong:"name='format',default='text',enum='text,json',help='Output format: text or json.'"`
}

// Run fetches and renders the storage usage report.
func (c *StorageUsageCmd) Run(cli *CLI) error {
	ctx := cli.rootContext()
	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()

	u, err := cn.Client().StorageUsage(ctx)
	if err != nil {
		return err
	}

	if c.Format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(u)
	}

	fmt.Printf("Registry CAS: %d blobs, %s\n", u.CASBlobs, humanBytes(u.CASBytes))
	if u.FileCacheFiles > 0 || u.FileCacheBytes > 0 {
		fmt.Printf("Block cache:  %d files, %s\n", u.FileCacheFiles, humanBytes(u.FileCacheBytes))
	}
	return nil
}

// humanBytes renders a byte count in binary units (KiB/MiB/…) with three
// significant figures, e.g. 1536 -> "1.5 KiB". Bytes below 1 KiB stay exact.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
