matches = search("deployment decision", limit=8)
emit([
    {
        "id": item["id"],
        "title": item["title"],
        "project": item["project"],
        "agent": item["agent"],
        "kind": item["kind"],
    }
    for item in matches
])
