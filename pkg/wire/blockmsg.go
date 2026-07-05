package wire

import "github.com/hugelgupf/p9/p9"

// Shared payload codecs for the block protocol's compound p9 types, used by both
// endpoints (blockproxy.go, blockserver.go) so their encodings can never drift.
// Scalar layout is big-endian via msgW/msgR (see blockproto.go).

func putQID(w *msgW, q p9.QID) {
	w.u8(uint8(q.Type))
	w.u32(q.Version)
	w.u64(q.Path)
}

func getQID(r *msgR) p9.QID {
	return p9.QID{Type: p9.QIDType(r.u8()), Version: r.u32(), Path: r.u64()}
}

func putQIDs(w *msgW, qids []p9.QID) {
	w.u16(uint16(len(qids)))
	for _, q := range qids {
		putQID(w, q)
	}
}

func getQIDs(r *msgR) []p9.QID {
	n := int(r.u16())
	if n == 0 {
		return nil
	}
	out := make([]p9.QID, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, getQID(r))
	}
	return out
}

func putAttrMask(w *msgW, m p9.AttrMask) {
	var v uint16
	set := func(b bool, bit uint) {
		if b {
			v |= 1 << bit
		}
	}
	set(m.Mode, 0)
	set(m.NLink, 1)
	set(m.UID, 2)
	set(m.GID, 3)
	set(m.RDev, 4)
	set(m.ATime, 5)
	set(m.MTime, 6)
	set(m.CTime, 7)
	set(m.INo, 8)
	set(m.Size, 9)
	set(m.Blocks, 10)
	set(m.BTime, 11)
	set(m.Gen, 12)
	set(m.DataVersion, 13)
	w.u16(v)
}

func getAttrMask(r *msgR) p9.AttrMask {
	v := r.u16()
	g := func(bit uint) bool { return v&(1<<bit) != 0 }
	return p9.AttrMask{
		Mode: g(0), NLink: g(1), UID: g(2), GID: g(3), RDev: g(4), ATime: g(5),
		MTime: g(6), CTime: g(7), INo: g(8), Size: g(9), Blocks: g(10), BTime: g(11),
		Gen: g(12), DataVersion: g(13),
	}
}

func putAttr(w *msgW, a p9.Attr) {
	w.u32(uint32(a.Mode))
	w.u32(uint32(a.UID))
	w.u32(uint32(a.GID))
	w.u64(uint64(a.NLink))
	w.u64(uint64(a.RDev))
	w.u64(a.Size)
	w.u64(a.BlockSize)
	w.u64(a.Blocks)
	w.u64(a.ATimeSeconds)
	w.u64(a.ATimeNanoSeconds)
	w.u64(a.MTimeSeconds)
	w.u64(a.MTimeNanoSeconds)
	w.u64(a.CTimeSeconds)
	w.u64(a.CTimeNanoSeconds)
	w.u64(a.BTimeSeconds)
	w.u64(a.BTimeNanoSeconds)
	w.u64(a.Gen)
	w.u64(a.DataVersion)
}

func getAttr(r *msgR) p9.Attr {
	return p9.Attr{
		Mode:             p9.FileMode(r.u32()),
		UID:              p9.UID(r.u32()),
		GID:              p9.GID(r.u32()),
		NLink:            p9.NLink(r.u64()),
		RDev:             p9.Dev(r.u64()),
		Size:             r.u64(),
		BlockSize:        r.u64(),
		Blocks:           r.u64(),
		ATimeSeconds:     r.u64(),
		ATimeNanoSeconds: r.u64(),
		MTimeSeconds:     r.u64(),
		MTimeNanoSeconds: r.u64(),
		CTimeSeconds:     r.u64(),
		CTimeNanoSeconds: r.u64(),
		BTimeSeconds:     r.u64(),
		BTimeNanoSeconds: r.u64(),
		Gen:              r.u64(),
		DataVersion:      r.u64(),
	}
}

func putSetAttrMask(w *msgW, m p9.SetAttrMask) {
	var v uint16
	set := func(b bool, bit uint) {
		if b {
			v |= 1 << bit
		}
	}
	set(m.Permissions, 0)
	set(m.UID, 1)
	set(m.GID, 2)
	set(m.Size, 3)
	set(m.ATime, 4)
	set(m.MTime, 5)
	set(m.CTime, 6)
	set(m.ATimeNotSystemTime, 7)
	set(m.MTimeNotSystemTime, 8)
	w.u16(v)
}

func getSetAttrMask(r *msgR) p9.SetAttrMask {
	v := r.u16()
	g := func(bit uint) bool { return v&(1<<bit) != 0 }
	return p9.SetAttrMask{
		Permissions: g(0), UID: g(1), GID: g(2), Size: g(3), ATime: g(4), MTime: g(5),
		CTime: g(6), ATimeNotSystemTime: g(7), MTimeNotSystemTime: g(8),
	}
}

func putSetAttr(w *msgW, a p9.SetAttr) {
	w.u32(uint32(a.Permissions))
	w.u32(uint32(a.UID))
	w.u32(uint32(a.GID))
	w.u64(a.Size)
	w.u64(a.ATimeSeconds)
	w.u64(a.ATimeNanoSeconds)
	w.u64(a.MTimeSeconds)
	w.u64(a.MTimeNanoSeconds)
}

func getSetAttr(r *msgR) p9.SetAttr {
	return p9.SetAttr{
		Permissions:      p9.FileMode(r.u32()),
		UID:              p9.UID(r.u32()),
		GID:              p9.GID(r.u32()),
		Size:             r.u64(),
		ATimeSeconds:     r.u64(),
		ATimeNanoSeconds: r.u64(),
		MTimeSeconds:     r.u64(),
		MTimeNanoSeconds: r.u64(),
	}
}

func putFSStat(w *msgW, s p9.FSStat) {
	w.u32(s.Type)
	w.u32(s.BlockSize)
	w.u64(s.Blocks)
	w.u64(s.BlocksFree)
	w.u64(s.BlocksAvailable)
	w.u64(s.Files)
	w.u64(s.FilesFree)
	w.u64(s.FSID)
	w.u32(s.NameLength)
}

func getFSStat(r *msgR) p9.FSStat {
	return p9.FSStat{
		Type: r.u32(), BlockSize: r.u32(), Blocks: r.u64(), BlocksFree: r.u64(),
		BlocksAvailable: r.u64(), Files: r.u64(), FilesFree: r.u64(), FSID: r.u64(),
		NameLength: r.u32(),
	}
}

func putDirents(w *msgW, ents p9.Dirents) {
	w.u32(uint32(len(ents)))
	for _, e := range ents {
		putQID(w, e.QID)
		w.u64(e.Offset)
		w.u8(uint8(e.Type))
		w.str(e.Name)
	}
}

func getDirents(r *msgR) p9.Dirents {
	n := int(r.u32())
	if n == 0 {
		return nil
	}
	out := make(p9.Dirents, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, p9.Dirent{
			QID:    getQID(r),
			Offset: r.u64(),
			Type:   p9.QIDType(r.u8()),
			Name:   r.str(),
		})
	}
	return out
}
