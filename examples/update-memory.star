updated = upsert_memory(
    key="stable-project-fact",
    body="One concise fact confirmed for future conversations.",
    kind="fact",
)
emit(updated)
