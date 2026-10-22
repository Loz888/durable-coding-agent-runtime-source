#!/usr/bin/env python3
"""Owned loopback-only Collector/Jaeger/Prometheus lab. No model calls or cloud resources."""
import argparse,json,os,pathlib,re,secrets,subprocess,time,urllib.request
REPO=pathlib.Path(__file__).resolve().parents[1]
TAGS={'COLLECTOR_IMAGE':'otel/opentelemetry-collector-contrib:latest','JAEGER_IMAGE':'jaegertracing/jaeger:latest','PROMETHEUS_IMAGE':'prom/prometheus:latest'}
def docker(*args):
 return subprocess.check_output(['docker',*args],text=True,stderr=subprocess.PIPE).strip()
def write(path,data):
 fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
 with os.fdopen(fd,'w') as f:json.dump(data,f,indent=2)
def inspect(identifier):return json.loads(docker('inspect',identifier))[0]
def owned(project,identifier):
 obj=inspect(identifier)
 if obj['Config']['Labels'].get('com.docker.compose.project')!=project:raise RuntimeError('resource ownership mismatch')
 return obj
def stop(directory):
 path=directory/'state.json';state=json.loads(path.read_text());project=state['project']
 if not re.fullmatch(r'runtime-observe-[a-f0-9]{24}',project):raise RuntimeError('invalid ownership token')
 ids=docker('ps','-aq','--no-trunc','--filter','label=com.docker.compose.project='+project).split()
 allowed={'collector','jaeger','prometheus'}
 for identifier in ids:
  obj=owned(project,identifier)
  if obj['Config']['Labels'].get('com.docker.compose.service') not in allowed:raise RuntimeError('unexpected resource retained for audit')
 for identifier in ids:docker('rm','-f',identifier)
 networks=docker('network','ls','-q','--filter','label=com.docker.compose.project='+project).split()
 for network in networks:
  obj=json.loads(docker('network','inspect',network))[0]
  if obj.get('Labels',{}).get('com.docker.compose.project')!=project:raise RuntimeError('network ownership mismatch')
  docker('network','rm',network)
 state['stopped']=True;state['owned_containers_remaining']=len(docker('ps','-aq','--filter','label=com.docker.compose.project='+project).split());write(path,state)
 print(json.dumps({'stopped':True,'owned_containers_remaining':state['owned_containers_remaining']}))
def start(directory,images_path):
 directory.mkdir(mode=0o700,parents=False,exist_ok=False)
 images={}
 if images_path:
  data=json.loads(images_path.read_text());images={x['requested_tag']:x['pinned_digest'] for x in data}
 pins={}
 for variable,tag in TAGS.items():
  if tag in images:digest=images[tag]
  else:
   obj=inspect(tag);digest=obj['RepoDigests'][0]
  expected=tag.rsplit(':',1)[0]
  if not re.fullmatch(re.escape(expected)+r'@sha256:[a-f0-9]{64}',digest):raise RuntimeError('preloaded official pinned digest required')
  pins[variable]=digest
 project='runtime-observe-'+secrets.token_hex(12);state={'project':project,'images':pins,'stopped':False,'scope':'local ephemeral validation; no paid backend, no external alert recipient'};write(directory/'state.json',state)
 envfile=directory/'images.env';envfile.write_text(''.join(k+'='+v+'\n' for k,v in pins.items()));envfile.chmod(0o600)
 command=['docker','compose','--project-name',project,'--env-file',str(envfile),'-f',str(REPO/'deploy/observability/compose.yaml'),'up','-d']
 with (directory/'startup.log').open('w') as log:
  if subprocess.run(command,stdout=log,stderr=subprocess.STDOUT).returncode:raise RuntimeError('startup failed; retain state and startup.log, then stop this instance')
 endpoints={};containers={}
 for service,port in [('collector',4318),('jaeger',16686),('prometheus',9090)]:
  identifier=docker('ps','-aq','--filter','label=com.docker.compose.project='+project,'--filter','label=com.docker.compose.service='+service)
  deadline=time.monotonic()+45
  while True:
   obj=owned(project,identifier);bindings=obj['NetworkSettings']['Ports'].get(str(port)+'/tcp') or []
   if bindings:break
   if obj['State']['Status']=='exited' or time.monotonic()>deadline:
    with (directory/(service+'.log')).open('w') as log:subprocess.run(['docker','logs',identifier],stdout=log,stderr=subprocess.STDOUT)
    raise RuntimeError('port readiness failed; retain private service log and stop this instance')
   time.sleep(.1)
  if len(bindings)!=1 or bindings[0]['HostIp']!='127.0.0.1':raise RuntimeError('non-loopback or missing port binding')
  endpoints[service]='http://127.0.0.1:'+bindings[0]['HostPort'];containers[service]=identifier
 state.update(endpoints=endpoints,containers=containers);write(directory/'state.json',state)
 deadline=time.monotonic()+45
 for service,suffix in [('jaeger','/'),('prometheus','/-/ready')]:
  while True:
   try:
    with urllib.request.urlopen(endpoints[service]+suffix,timeout=2) as r:
     if r.status==200:break
   except Exception:pass
   if time.monotonic()>deadline:
    for name,identifier in containers.items():
     with (directory/(name+'.log')).open('w') as log:subprocess.run(['docker','logs',identifier],stdout=log,stderr=subprocess.STDOUT)
    raise RuntimeError('backend readiness failed; private logs and exact cleanup state retained')
   time.sleep(.25)
 print(json.dumps({'endpoints':endpoints,'scope':state['scope']}))
def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('action',choices=['start','stop']);parser.add_argument('--dir',required=True,type=pathlib.Path);parser.add_argument('--images-json',type=pathlib.Path);args=parser.parse_args();directory=args.dir.absolute()
 if args.action=='start':start(directory,args.images_json)
 else:stop(directory)
if __name__=='__main__':
 try:main()
 except Exception as e:raise SystemExit('Local observability: '+str(e))
