// Package git is reserved for native Git adapters over rebuildable bare repositories.
// Read snapshot commit/tree/blob objects directly. Track upstream refs separately
// from retention refs so force-pushes and deleted branches cannot erase retained snapshots.
package git
