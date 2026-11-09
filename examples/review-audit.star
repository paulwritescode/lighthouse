# Inspect the latest data mutations and sensitive-review markers.
changes = audit(limit=20)
emit(changes)
