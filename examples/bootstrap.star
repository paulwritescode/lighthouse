pack = load_memory(limit=12)
emit({
    "snapshot_id": pack["snapshot_id"],
    "revision": pack["revision"],
    "context": pack["context"],
})
