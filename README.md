# simpleconf

Simple way to read and write configurations in a cluster, without single point of failure(SPOF).

## config.yml

- db: appendonly database file
- listen: frontend endpoint

## example

```bash
echo '2017' | http http://localhost:23456/db/product.year
echo '"Demo"' | http http://localhost:23456/db/product.name
echo 'false' | http http://localhost:23456/db/product.is_expired

http http://localhost:23456/db/product
{
    "is_expired": false, 
    "name": "Demo", 
    "year": 2017
}

http delete http://localhost:23456/db/product.is_expired

http http://localhost:23456/db/product
{
    "name": "Demo", 
    "year": 2017
}
```

## benchmarks

```
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkGet-16      	35865764	       31.97 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-16      	 7825952	       153.1 ns/op	      96 B/op	       3 allocs/op
BenchmarkDel-16      	12272269	       96.00 ns/op	      80 B/op	       3 allocs/op
BenchmarkClone-16    	 5811598	       205.7 ns/op	     168 B/op	       4 allocs/op
```

```
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpGet-16      	  202935	      5271 ns/op
BenchmarkTcpSet-16      	  227640	      5764 ns/op
BenchmarkTcpDel-16      	  195621	      5723 ns/op
BenchmarkTcpClone-16    	  220393	      5961 ns/op
```

wrk read

```
Running 10s test @ http://127.0.0.1:23456/db
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    40.02us   13.04us   2.34ms   79.62%
    Req/Sec   103.13k     4.05k  110.66k    65.35%
  2072219 requests in 10.10s, 310.27MB read
Requests/sec: 205173.65
Transfer/sec:     30.72MB
```

wrk write

```
Running 10s test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    57.63us   24.61us 839.00us   89.99%
    Req/Sec    77.99k     1.84k   83.63k    70.30%
  1567237 requests in 10.10s, 186.83MB read
Requests/sec: 155171.47
Transfer/sec:     18.50MB
```

## interfaces

#### Get whole configurations

`GET /db`

TCP:
`=`

Returns raw JSON, in case of dump the database, don't use it often

#### Get values with key path

`GET /db/{key.path}`

TCP:
`=key.path`

Returns raw JSON, use key path as fine-grained as possible

#### Set values by key path

`PUT /db/{key.path} {"name": "Demo"}`

TCP:
```
+key.path
{"name": "Demo"}
```

Put any of raw JSON body

#### Delete values key path

`DELETE /db/{key.path}`

TCP:
`-key.path`

#### Clone values between key path

`POST /clone/{from.key.path}/{to.key.path}`

TCP:
```
<from.key.path
>to.key.path
```

#### Rewrite appendonly database file

`POST /vacuum`

TCP:
`*`

#### TCP ping

`PING`

## License 

MIT
