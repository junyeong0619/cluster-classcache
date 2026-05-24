# classcache CLI — C
#
# Deps (macOS): brew install hiredis cjson
# Deps (Debian): apt-get install libhiredis-dev libcjson-dev libcurl4-openssl-dev
#
# Build:   make
# Install: install ./build/classcache /usr/local/bin/  (or symlink as
#          kubectl-classcache so `kubectl classcache stats` works)

CC      ?= cc
CFLAGS  ?= -O2 -g -Wall -Wextra -Wno-unused-parameter -std=c11
LDFLAGS ?=
PREFIX  ?= /usr/local

# Header / library paths (Homebrew prefers /opt/homebrew on Apple Silicon).
ifeq ($(shell uname),Darwin)
BREW := $(shell brew --prefix 2>/dev/null)
CFLAGS  += -I$(BREW)/include
LDFLAGS += -L$(BREW)/lib
endif

LIBS = -lhiredis -lcjson -lcurl

SRCS = $(wildcard src/*.c)
OBJS = $(patsubst src/%.c,build/%.o,$(SRCS))
BIN  = build/classcache

.PHONY: all clean install

all: $(BIN)

$(BIN): $(OBJS)
	$(CC) $(CFLAGS) -o $@ $^ $(LDFLAGS) $(LIBS)

build/%.o: src/%.c | build
	$(CC) $(CFLAGS) -Iinclude -c -o $@ $<

build:
	mkdir -p build

clean:
	rm -rf build

install: $(BIN)
	install -m 0755 $(BIN) $(PREFIX)/bin/classcache
	# Optional: also expose as kubectl plugin.
	ln -sf $(PREFIX)/bin/classcache $(PREFIX)/bin/kubectl-classcache
