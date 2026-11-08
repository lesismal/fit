A pipeline's `source` reads its records: one of these, named by `module`, with its `params`. A
source resumes from the pipeline's checkpoint, and gives each record the position it resumes
after.

| Module | What it does |
| --- | --- |
| [`generate`](#sync-sources-generate) | Numbered records of every value type, for trials and tests |
