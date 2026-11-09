# Replace this ID with the source ID returned by search() or save().
# Unknown keys become document-scoped JSON fields; they do not alter SQL schema.
result = update(
    id="replace-with-artifact-id",
    fields={"review_status": "verified", "confidence": 0.94},
)
emit(result)
