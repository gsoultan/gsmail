### Project Guidelines

#### 1. Build and Configuration
To optimize for speed, size, and zero allocation, use the following build flags:
- **Optimization for Speed and Size**:
  ```bash
  go build -ldflags="-s -w" -gcflags="-m -l" -o gsmail main.go
  ```
  - `-s -w`: Strips debug information and symbol tables to reduce binary size.
  - `-gcflags="-m -l"`: `-m` prints optimization decisions (like escape analysis) to help achieve zero allocations, and `-l` disables inlining if needed (though usually kept for speed).

- **Zero Allocation Practices**:
  - Use `sync.Pool` for frequently allocated objects.
  - Avoid interface conversions in hot paths.
  - Use `[]byte` buffers instead of string concatenations where possible.
  - Check escape analysis using `go build -gcflags="-m"`.

#### 2. Testing Information
- **Running Tests**:
  - Run all tests: `go test ./...`
  - Run tests with race detection: `go test -race ./...`
  - Run benchmarks: `go test -bench=. -benchmem ./...`

- **Adding New Tests**:
  - Create a file with `_test.go` suffix.
  - Use the `testing` package.
  - Follow the Uber Go Style Guide for test structure.

- **Example Test**:
  ```go
  package gsmail

  import "testing"

  func TestSample(t *testing.T) {
      got := 1
      want := 1
      if got != want {
          t.Errorf("got %d, want %d", got, want)
      }
  }
  ```

#### 3. Development Style
- **Code Style**: This project follows the [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md).
- **Key Principles**:
  - Keep interfaces small.
  - Handle errors explicitly.
  - Use meaningful variable names.
  - Avoid global state.
