# Memory Save

Store a durable note that should survive this session: a user preference, a
project fact, or a decision the team agreed on.

Save when the user says "remember", when you discover a command or
convention you will need again, or when a decision was made for a reason
that is not obvious from the code. Do not save task progress, plans, or
anything already written down in the repository.

`type` is one of `user`, `project` or `team`. Never store secrets,
credentials or personal data. Keep the title short; put the detail in
`content`. Reuse an existing `id` to update an entry instead of creating a
near-duplicate.
