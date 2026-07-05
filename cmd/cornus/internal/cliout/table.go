package cliout

// Table accumulates rows and renders them on Flush. In plain and fancy modes it
// is an aligned text/tabwriter table (fancy bolds the header); in json mode it
// emits one object per row keyed by header. Tables are results, so they render
// to stdout.
type Table struct {
	d       *Driver
	headers []string
	rows    [][]string
}

// Table starts a table with the given column headers.
func (d *Driver) Table(headers ...string) *Table {
	return &Table{d: d, headers: headers}
}

// Row appends a row. Cells align to the header columns; extra or missing cells
// are tolerated.
func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

// Flush renders the accumulated table to stdout.
func (t *Table) Flush() error {
	return t.d.r.table(t.d.out, t.headers, t.rows)
}

// KVBlock accumulates key/value pairs and renders them aligned on Flush ("key:
// value"), or as a single JSON object in json mode. It renders to stdout.
type KVBlock struct {
	d     *Driver
	pairs [][2]string
}

// KV starts a key/value block.
func (d *Driver) KV() *KVBlock { return &KVBlock{d: d} }

// Add appends a key/value pair.
func (k *KVBlock) Add(key, value string) *KVBlock {
	k.pairs = append(k.pairs, [2]string{key, value})
	return k
}

// Flush renders the accumulated pairs to stdout.
func (k *KVBlock) Flush() error {
	return k.d.r.kv(k.d.out, k.pairs)
}
