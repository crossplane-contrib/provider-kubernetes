FROM BASEIMAGE
RUN apk --no-cache add ca-certificates bash

ADD provider /usr/local/bin/crossplane-kubernetes-provider

ENV XDG_CACHE_HOME /tmp

EXPOSE 8080
USER 1001
ENTRYPOINT ["crossplane-kubernetes-provider"]