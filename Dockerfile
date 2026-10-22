ARG BASE_IMAGE=registry-cbu.huawei.com/atelier/pytorch_ascend:pytorch_2.5.1-cann_8.1.rc2-py_3.11-hce_2.0.2503-aarch64-snt9b23-20250711103553-b3d23bf
FROM ${BASE_IMAGE}
USER root

ARG route_server_work_dir=/opt/Deploy/
RUN mkdir -p ${route_server_work_dir}
WORKDIR ${route_server_work_dir}

COPY . ${route_server_work_dir}
