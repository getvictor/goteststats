# goteststats

Article about [accurately measuring execution time of Go tests](https://victoronsoftware.com/posts/go-test-execution-time/).

## Install and run

Generate a `result.json` file with the output of `go test -json ./...`.

Clone the repository and run the following command:

```bash
cat result.json | go run main.go
```
