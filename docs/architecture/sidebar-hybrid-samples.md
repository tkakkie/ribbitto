# Cost-harness persist-to-exact samples (#952c2b)

Part #952c2b derives persist-to-exact samples for every post/stream pair and
changed read/bound stream. Before timing, the fixture pool captures message
identities and list metadata. After writers join, independent enumeration adds
successful posts and applies each witness's sequence boundary, member join
boundary and scoped feed prefix; hashing that expectation checks the full model
without oracle SQL on the timed path. The first digest-equal sender witness
covering the change ends its sample. The sample horizon includes the runner's
allowed drain; sends after that cutoff supply no completion. Missing commit
stamps, absent exact sends and every sample on an overflowed stream are counted
incomplete, with overflowed streams also reported separately.

Normal scheduled recounts, synthetic deltas and periodic repairs are separate
categories. A periodic tick pending when computation starts labels that recount
periodic, including coalesced normal triggers; ticks during computation remain
pending for the next recount and cannot relabel an in-flight frame. Defensive
coverage retries alone are normal recounts; recovery metrics remain #952c3.
Production-only routing sends no deltas: zero delta samples and `evidence=false`
are explicit, never passing delta evidence. Synthetic delta witnesses verify
classification only. Virtual-time/gated tests and mutation checks verify all
categories, exactness, first-send selection and incomplete/overflow handling;
mutation commands and assertions belong in the PR body. No measurement ran.
