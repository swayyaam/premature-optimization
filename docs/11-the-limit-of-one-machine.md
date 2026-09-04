# The limit of one machine

**Code:** none. This is not a step. It is a description of the boundary the previous ten
ran into, and of what sits on the other side of it.

## The two numbers

Everything in this repo was measured on one laptop. Two figures came out of it and
neither one moved no matter what was changed:

```
an endpoint that talks to nothing        about 90,000 requests per second
an endpoint that makes one round trip    about 42,000 requests per second
```

Both are flat from 25 concurrent requests to 800. Latency rises in proportion to
concurrency while throughput stays put, which is what a limit looks like rather than a
queue.

The gap between them is one round trip to a backend, and it costs about 48,000 requests
per second whether Redis or Postgres is at the far end.

Everything after step 3 was measured against a ceiling of 42,000, and nothing built after
step 3 moved it.

## What is actually holding the ceiling

Four things run on this machine during a load test, and all four share the same
processors.

```
                    cache warm     cache off
application            ~365%         ~381%
load generator         ~230%         ~234%
Redis                   ~62%            0%
Postgres                  0%          ~213%
                    ---------     ---------
                       ~657%         ~828%
```

Those figures were sampled while the tests were running, on a machine reported as having
ten cores.

Ten cores is a misleading number here. Four of them are performance cores and six are
efficiency cores, which are considerably slower. A total of 657% spread across that mix
is much closer to full than it looks written down.

Three separate things follow from that table.

**The load generator is a quarter of the load.** `hey` uses about 230% of CPU to produce
this traffic, which is more than Redis and Postgres combined in the warm case. Every
request is being both sent and received by the same machine, so the measurement is
competing with the thing it measures. A real client is somewhere else and costs this
server nothing.

**The round trip is not a network cost.** Everything talks over the loopback interface.
There is no network card, no driver, no cable, no switch, no packet loss and no distance.
What the round trip actually costs is system calls, copying bytes between the kernel and
the process, and encoding and decoding a protocol. That work happens in the application
process, and it is why the application sits above 350% of CPU while the database it is
talking to sits at zero.

**More application processes do not help.** Step 6 measured this directly. Three instances
behind nginx came out 14% slower than one, because the copies divide the same processors
between them and the proxy takes a share as well.

## What would have to change

To move meaningfully past these numbers, the shape of the setup has to change rather than
the code.

**Move the load generator to another machine.** This is the first change and it is not
optional, because roughly 230% of CPU is currently being spent producing traffic rather
than serving it. It also makes the client side real: connections arrive over a network
card, with the latency and the loss that implies, instead of over loopback.

**Move Postgres and Redis to their own machines.** This gives the application the whole
of its own processor back. It also makes every backend call slower on its own, because a
loopback round trip is measured in tens of microseconds and a real one over a local
network is measured in hundreds. Throughput per connection goes down and total capacity
goes up. Those pull in opposite directions and only measurement settles where the balance
lands.

**Then run several application machines behind a load balancer.** This is the point at
which horizontal scaling does what people expect. Three machines have three times the
processors. Three processes on one machine never did.

Once that is in place the limit moves somewhere new, and the techniques for chasing it are
different ones: reusing connections between hops, batching requests, tuning the kernel's
network settings, spreading interrupts across cores, and measuring the network itself as
carefully as this repo measured the database.

## Why that is a different project

The change is not one of scale. It is a change of subject.

Every question in this repo could be answered by running one program and reading one
number. Was the query using the index. Was the pool churning connections. Did the cache
change the mean or the tail. Each of those has a definite answer that a single machine can
produce in twenty seconds.

The questions on the other side of the boundary do not work that way.

- How far behind is the replica, and what does a read that lands on a stale one do to a
  user.
- What happens to requests in flight when a machine is being replaced.
- How does an instance find the database after the database moves.
- Which of these components can be unavailable without the others noticing, and which
  cannot.
- What does the system do when the network between two parts of it is slow rather than
  broken.

None of those are performance questions. They are questions about a system with more than
one machine in it, and they need more than one machine to ask. They also need the
surrounding apparatus: provisioning, deployment, service discovery, monitoring that
collects from several places at once. That apparatus is most of the work, and none of it
is about making a Go handler faster.

That is a good project. It is not this one.

## What this project can and cannot tell you

It can tell you that an unindexed lookup on 500,000 rows costs a factor of 24, that
`database/sql` defaults will churn connections until you stop them, that a cache can
improve the tail and leave the mean alone, that a slow dependency without a deadline is an
outage, and that percentiles over a whole run will hide that outage completely. None of
those depend on how many machines are involved. They are properties of the code, and they
would be true on any hardware.

It cannot tell you what this software does on real hardware, and no measurement taken this
way ever could. Every number here is a comparison between two versions run minutes apart
on the same laptop. That is what it was built to produce, and it is worth being clear that
it is all it produces.

## The finish

The plan was ten steps ending in database scaling. The measurement said the database was
never the limit, so that step was written up as a decision instead of a change.

This document is the other half of that decision. Not building the replica was the right
call for a specific reason, and the reason is that the boundary is somewhere else
entirely: it is the machine, and one machine is as far as this particular question goes.

Knowing exactly where the wall is, and what it is made of, is a better place to stop than
a number would have been.
