# Inspect the available document fields and locate one source record before an edit.
schema = describe(project="lighthouse")
matches = search(query="storage choice", project="lighthouse", limit=5)
emit({"schema": schema, "matches": matches})
