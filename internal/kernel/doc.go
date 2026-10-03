// Package kernel holds what every module shares and no module naturally owns
// (decision 26). It is not a home for shared things that are hard to place:
// only IDs, stable value types, and cross-module contracts with no natural
// single owner belong here. Today that is ID. It imports nothing internal.
package kernel
