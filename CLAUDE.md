# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go client library for the flags.gg feature flag management system. The library provides a simple interface for checking feature flags with built-in caching, circuit breaking, and environment variable overrides.

## Architecture

The codebase follows a clean architecture pattern with these key components:

- **Client (`flags.go`)**: Main entry point that handles API communication, caching strategy, and circuit breaking
- **Cache Interface (`cache/cache.go`)**: Defines the caching contract with two implementations:
  - Memory cache (`cache/memory.go`): Uses sync.Map for thread-safe in-memory storage
  - SQLite cache (`cache/sqlite.go`): Persistent storage using SQLite database
- **Flag Types (`flag/flag.go`)**: Defines FeatureFlag and Details structs for flag data
- **Thread Safety**: Uses sync.RWMutex throughout for concurrent access protection
- **Circuit Breaker**: Implements failure detection to prevent cascading failures when the API is unavailable

## Development Commands

### Running Tests
```bash
# Run all tests with verbose output
go test -v ./...

# Run tests with benchmarks and memory profiling
go test -v -bench=./... -benchmem -timeout=120s ./...

# Run tests for a specific package
go test -v ./cache/...

# Run a specific test
go test -v -run TestClientIs ./...
```

### Code Quality
The project uses Qodana for static analysis. It runs automatically in CI, but you can also run it locally if you have Qodana CLI installed.

### Building
```bash
# Download dependencies
go mod download

# Build the library (this is a library, so no binary is produced)
go build ./...

# Verify the module
go mod verify
```

## Key Implementation Details

1. **Authentication**: Requires Project ID, Agent ID, and Environment ID passed via headers
2. **Caching Strategy**: 
   - Default uses SQLite for persistence across restarts
   - Optional in-memory cache for performance-critical applications
   - Cache refresh interval is determined by the API response
3. **Environment Overrides**: Flags can be overridden locally using environment variables with the `FLAGS_` prefix (e.g., `FLAGS_MY_FEATURE=true`)
4. **Error Handling**: The client gracefully handles API failures by falling back to cached values
5. **Concurrent Access**: All operations are thread-safe using read/write mutexes

## Testing Approach

- Tests use Go's standard testing package with table-driven test patterns
- HTTP test servers mock the flags.gg API responses
- Tests cover error conditions, concurrent access, and edge cases
- Both cache implementations have dedicated test files ensuring consistency

## Common Tasks

### Adding a New Cache Implementation
1. Implement the `cache.Cache` interface in a new file under `/cache/`
2. Add corresponding tests following the pattern in `flags_memory_test.go` or `flags_sqlite_test.go`
3. Add a new `WithXXX()` option function in `flags.go` to enable the new cache type

### Modifying Flag Response Structure
1. Update the structs in `/flag/flag.go`
2. Ensure backward compatibility or update all references
3. Update tests to reflect the new structure

### Debugging API Communication
- Set appropriate log levels in your application
- The client logs errors when API calls fail
- Check the circuit breaker state if requests are being skipped