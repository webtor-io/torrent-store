# lazymap

Golang thread-safe LazyMap implementation with additional features:

1. Concurrency control
2. Capacity control (if defined, it automatically cleans least recently used elements)
3. Expiration (if defined, it automatically deletes expired elements)

The main purpose of LazyMap at [webtor.io](//webtor.io) was to reduce requests between services by introducing intermediate caching-layer.

## But what is LazyMap anyway?

Unlike ordinary Map, LazyMap evaluates value on demand if there is no value already exists for a specific key.

## Example

```golang
package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/webtor-io/lazymap"
)

type ResponseMap struct {
	lazymap.LazyMap
	cl *http.Client
}

func NewResponseMap(cl *http.Client) *ResponseMap {
	return &ResponseMap{
		cl: cl,
		LazyMap: lazymap.New(&lazymap.Config{
			Concurrency: 10,
			Expire:      600 * time.Second,
			ErrorExpire: 30 * time.Second,
			Capacity:    1000,
		}),
	}
}
func (s *ResponseMap) get(u string) (string, error) {
	res, err := s.cl.Get(u)
	if err != nil {
		return "", err
	}
	b := res.Body
	defer b.Close()
	r, err := ioutil.ReadAll(b)
	if err != nil {
		return "", err
	}
	return string(r), nil
}

func (s *ResponseMap) Get(u string) (string, error) {
	v, err := s.LazyMap.Get(u, func() (interface{}, error) {
		return s.get(u)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func main() {
	rm := NewResponseMap(http.DefaultClient)
	res, err := rm.Get("https://example.org")
	if err != nil {
		log.Fatal(err)
	}
	log.Println(res)
}
```
