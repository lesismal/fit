// gen writes dirs x files random files of size bytes under root: root/dNNNN/fNNNN,
// and root/hot.bin, with workers goroutines.
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
)

func main() {
	root := flag.String("root", "", "")
	dirs := flag.Int("dirs", 2560, "")
	files := flag.Int("files", 820, "")
	size := flag.Int("size", 8192, "")
	workers := flag.Int("workers", 8, "")
	flag.Parse()
	work := make(chan int, 64)
	var wg sync.WaitGroup
	for w := range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewChaCha8([32]byte{byte(w)}))
			buf := make([]byte, *size)
			for d := range work {
				dir := filepath.Join(*root, fmt.Sprintf("d%04d", d))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					panic(err)
				}
				for f := range *files {
					for i := 0; i < len(buf); i += 8 {
						v := r.Uint64()
						for k := 0; k < 8 && i+k < len(buf); k++ {
							buf[i+k] = byte(v >> (8 * k))
						}
					}
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", f)), buf, 0o644); err != nil {
						panic(err)
					}
				}
			}
		}()
	}
	for d := range *dirs {
		work <- d
	}
	close(work)
	wg.Wait()
	buf := make([]byte, *size)
	for i := range buf {
		buf[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(*root, "hot.bin"), buf, 0o644); err != nil {
		panic(err)
	}
}
