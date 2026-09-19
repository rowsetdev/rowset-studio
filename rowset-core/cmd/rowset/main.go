package main

import "github.com/rowsetdev/rowset-studio/rowset-core/rowset"

var version = "dev"

func main() {
	rowset.Main(rowset.Options{Version: version})
}
