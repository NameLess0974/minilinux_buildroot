CC = gcc
CFLAGS = -Wall -O2
LDFLAGS = -static -lm
TARGET = fb_gif

all: $(TARGET)

$(TARGET): main.c
	$(CC) $(CFLAGS) -o $@ $^ $(LDFLAGS)

clean:
	rm -f $(TARGET)
