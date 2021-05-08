#!/bin/sh

# sudo docker run --rm=true -itv $PWD:/mnt debian:buster-slim /mnt/build_docker_bin.sh

set -ex

apt update
command -v go version >/dev/null 2>&1 || { apt install -y golang; }
apt install -y autoconf automake libtool pkg-config make gcc zlib1g-dev libssl-dev openssl git wget

cd /mnt
rm -rf libbaresip
rm -f go-baresip
mkdir libbaresip
cd libbaresip/
mkdir git
mkdir re
mkdir rem
mkdir baresip
cd git

my_base_modules="autotest ctrl_tcp debug_cmd httpd menu ice stun turn uuid account contact"
my_audio_modules="aubridge aufile auloop"
my_codec_modules="g711"
#my_ui_modules="stdio cons"
my_tls_modules="srtp"

git clone https://github.com/baresip/re.git
cd re; make libre.a; cp libre.a ../../re; cd ..

git clone https://github.com/baresip/rem.git
cd rem; make librem.a; cp librem.a ../../rem; cd ..

git clone https://github.com/baresip/baresip.git
git clone https://github.com/baresip/baresip-apps.git

mv baresip-apps/modules/autotest baresip/modules/
sed -i 's/$(BARESIP_MOD_MK)/mk\/mod.mk/g' baresip/modules/autotest/module.mk
sed -i '/auloop/a MODULES   += autotest' baresip/mk/modules.mk

cd baresip
    
make LIBRE_SO=../re LIBREM_PATH=../rem STATIC=1 libbaresip.a \
    MODULES="$my_base_modules $my_audio_modules $my_codec_modules $my_ui_modules $my_tls_modules"

cp libbaresip.a ../../baresip; cd ..
mv re/include ../re
mv rem/include ../rem
mv baresip/include ../baresip
cd ..; rm -rf git; cd ..

cd espeak
if [ ! -d "espeak-ng" ]; then
    git clone https://github.com/espeak-ng/espeak-ng.git
fi
cd espeak-ng
./autogen.sh
./configure --without-async --without-mbrola --without-sonic --without-speechplayer
make
cp src/.libs/libespeak-ng.a ../
cp src/include/espeak-ng/speak_lib.h ../
make clean
cd ..
rm -rf espeak-ng
cd ..

go build -ldflags "-w"  -o go-baresip-demo/go-baresip-demo go-baresip-demo/*.go
