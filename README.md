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

cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpSet-16      	   51232	     22851 ns/op
BenchmarkTcpGet-16      	   61987	     19295 ns/op
BenchmarkTcpClone-16    	   53688	     22532 ns/op
BenchmarkTcpDel-16      	   54684	     22070 ns/op
```

TCP

```
Running 10s GET test @ 127.0.0.1:23466
  500 connections
  Stats		Avg		Min		Max
  Req/Sec	659.187µs	13.841777ms	17.172µs
  4928439 requests in 10.000008278s
Requests/sec: 492843.59

Running 10s SET test @ 127.0.0.1:23466
  100 connections
  Stats		Avg		Min		Max
  Req/Sec	300.293µs	2.8153ms	18.214µs
  2874593 requests in 10.000167975s
Requests/sec: 287454.47

Running 10s DELETE test @ 127.0.0.1:23466
  100 connections
  Stats		Avg		Min		Max
  Req/Sec	237.16µs	3.515856ms	17.332µs
  3393076 requests in 10.000016297s
Requests/sec: 339307.05
```

wrk

```
Running 10s GET test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    40.06us   10.91us 738.00us   76.41%
    Req/Sec   104.03k     4.36k  113.08k    65.35%
  2091039 requests in 10.10s, 295.14MB read
Requests/sec: 207040.39
Transfer/sec:     29.22MB

Running 10s SET test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    66.40us   34.08us   1.21ms   90.89%
    Req/Sec    72.18k     1.03k   75.19k    68.81%
  1450633 requests in 10.10s, 172.93MB read
Requests/sec: 143629.05
Transfer/sec:     17.12MB

Running 10s DELETE test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    53.39us   41.82us   3.48ms   96.62%
    Req/Sec    86.50k     2.24k   95.54k    69.00%
  1721124 requests in 10.00s, 205.17MB read
Requests/sec: 172110.01
Transfer/sec:     20.52MB
```

## interfaces

#### Get whole configurations

HTTP:
`GET /db`

TCP:
`=`

Returns raw JSON, in case of dump the database, don't use it often

#### Get values with key path

HTTP:
`GET /db/{key.path}`

TCP:
`=key.path`

Returns raw JSON, use key path as fine-grained as possible

#### Set values by key path

HTTP:
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

HTTP:
`POST /clone/{from.key.path}/{to.key.path}`

TCP:
```
<from.key.path
>to.key.path
```

#### Rewrite appendonly database file

HTTP:
`POST /vacuum`

TCP:
`*`

#### TCP ping

`PING`

## License 

MIT
