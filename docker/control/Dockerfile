FROM golang:1.16


# Use mirrors for poor network...
#RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list && \
#    sed -i 's/security.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list


# tiup-cluster dependencies
# vim is not required by tiup-cluster itself, just for ease of use
RUN apt-get -y -q update && \
    apt-get -y -q install software-properties-common && \
    apt-get install -qqy \
        default-mysql-client \
        psmisc \
        vim && \
    rm -rf /var/lib/apt/lists


# without --dev flag up.sh copies tiup-cluster to these subfolders
# with --dev flag they are empty until mounted
COPY tiup-cluster/tiup-cluster /tiup-cluster/tiup-cluster/
COPY tiup-cluster /tiup-cluster/

ADD bashrc /root/.bashrc
ADD init.sh /init.sh
RUN chmod +x /init.sh && \
    mkdir -p /root/.ssh && \
    echo "Host *\n    ServerAliveInterval 30\n    ServerAliveCountMax 3" >> /root/.ssh/config

# build tiup-cluster in without --dev flag
WORKDIR /tiup-cluster
RUN (test Makefile && make failpoint-enable && make cluster && make failpoint-disable) || true

CMD /init.sh
