tabc - Time-Aware Byte Consolidator
======

[![Build Status](https://travis-ci.org/thejerf/tabc.png?branch=master)](https://travis-ci.org/thejerf/tabc)

Current status: Still developing - completely unusable. Check back in
a couple of weeks.

This package provides code that accepts bytes input over time as `[]byte`,
and allows you to consolidate that stream into something more meaningful
for your task.

The initial code was written to take a stream of keystrokes obtained in
real time from a shell session and do a heuristic job of turning it back
into individual commands. So, for instance, if a human typed `ls -l
~/projects\n`, on a real-time shell that is very likely to result in an
individual packet for each letter. tabc provides code that allows you to
easily re-assemble those keystrokes into a full line. This is useful for
providing easier-to-read logs for auditing purposes.

You can provide callbacks that can also further break up the code, so
for instance, if someone copied and pasted six commands into the terminal,
resulting in a single packet, you can provide a callback that will also
break that up along newlines, so the output emits one line at a time.

Because a tabc needs to emit a timely result when the timeout occurs, each
ByteConsolidator has its own goroutine. Without a goroutine, a
ByteConsolidator would be dependent on being called in some manner in order
to produce a result. However, this does require you to be aware that there
is a goroutine involved, so where you may normally expect to be able to
compose io.Readers together without worrying about additional goroutines,
you will need to consider that here. (It is likely that naive use is still
correct, it is just something you need to bear in mind.)

Do Not Use This Package To...
-----------------------------

 * ...gather potentially broken-up network packets: You should generally
   use [io.ReadFull](https://golang.org/pkg/io/#ReadFull) or something
   similar.

Code Signing
------------

I will be signing this repository with
the ["jerf" keybase account](https://keybase.io/jerf). If you are viewing
this repository through GitHub, you should see the commits as showing as
"verified" in the commit view.

(Bear in mind that due to the nature of how git commit signing works, there
may be runs of unverified commits; what matters is that the top one is signed.)

Changelog
---------

tabc uses semantic versioning.

1. v0.0.1 - initial release
