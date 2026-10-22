#!/usr/bin/env python3
"""Restore only a new owned synthetic fixture into a second private PG cluster.
No production database, global authentication, user passwords or API keys are read.
"""
import argparse,hashlib,json,os,pathlib,re,shutil,subprocess,tempfile,time,uuid

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--source-socket',required=True);ap.add_argument('--source-port',type=int,required=True);ap.add_argument('--output',required=True);args=ap.parse_args()
 out=pathlib.Path(args.output).absolute();out.mkdir(mode=0o700,parents=False,exist_ok=False)
 if not pathlib.Path(args.source_socket).is_absolute() or not 1<=args.source_port<=65535:raise RuntimeError('explicit owned Unix-socket cluster required')
 pg=pathlib.Path('/usr/lib/postgresql/16/bin');required=['initdb','pg_ctl','pg_dump','pg_dumpall','pg_restore','createdb','dropdb','psql']
 if any(not (pg/x).is_file() for x in required):raise RuntimeError('PostgreSQL 16 utilities required')
 env={k:v for k,v in os.environ.items() if not any(x in k.upper() for x in ['DATABASE_URL','API_KEY','PGPASSWORD'])};env.update(PGHOST=args.source_socket,PGPORT=str(args.source_port),PGDATABASE='postgres',PGCONNECT_TIMEOUT='5')
 token=uuid.uuid4().hex[:12];database='restore_fixture_'+token;reader='restore_reader_'+token
 def run(argv,environment=env,input=None,check=True):
  r=subprocess.run([str(pg/argv[0])]+argv[1:],env=environment,input=input,capture_output=True,text=isinstance(input,str) or input is None,timeout=60)
  if check and r.returncode:raise RuntimeError('scoped PostgreSQL operation failed: '+argv[0])
  return r
 def sql(query,environment=env,check=True):return run(['psql','-X','-v','ON_ERROR_STOP=1','-At'],environment,query,check)
 ownDB=False;ownRole=False;started=False;socketDir=pathlib.Path(tempfile.mkdtemp(prefix='runtime-pg-restore-'));target=env.copy();target.update(PGHOST=str(socketDir),PGPORT='18570',PGDATABASE='postgres');result={'scope':'synthetic fresh database, independent local PG cluster; not a production backup or physical-host recovery','status':'FAIL','cleanup':False}
 try:
  version=sql('SHOW server_version;').stdout.strip();result['postgres_version']=version
  # Every mutation is an exact generated resource created by this invocation.
  sql('CREATE ROLE '+reader+' NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;');ownRole=True
  run(['createdb',database]);ownDB=True;source=env.copy();source['PGDATABASE']=database
  sql('CREATE SCHEMA fixture; CREATE TABLE fixture.items(id integer PRIMARY KEY,value text NOT NULL); INSERT INTO fixture.items VALUES(1,\'synthetic checkpoint\'),(2,\'synthetic patch\'); GRANT USAGE ON SCHEMA fixture TO '+reader+'; GRANT SELECT ON fixture.items TO '+reader+';',source)
  dump=out/'fixture.dump';run(['pg_dump','--format=custom','--file',str(dump)],source)
  roles=run(['pg_dumpall','--roles-only','--no-role-passwords']).stdout
  lines=[line for line in roles.splitlines() if line.startswith('CREATE ROLE '+reader+';') or line.startswith('ALTER ROLE '+reader+' WITH ')]
  if len(lines)!=2 or any('PASSWORD' in line or ('SUPERUSER' in line and 'NOSUPERUSER' not in line) for line in lines):raise RuntimeError('unexpected fixture role metadata')
  roleSQL='\n'.join(lines)+'\n';(out/'fixture-role.sql').write_text(roleSQL);os.chmod(out/'fixture-role.sql',0o600)
  run(['initdb','-D',str(out/'pg'),'-A','trust','--no-instructions'],target)
  config=out/'pg'/'postgresql.conf'
  with config.open('a') as f:f.write("\nlisten_addresses=''\nunix_socket_directories='"+str(socketDir).replace("'","''")+"'\nport=18570\n")
  run(['pg_ctl','-D',str(out/'pg'),'-l',str(out/'postgres.log'),'-w','start'],target);started=True
  sql(roleSQL,target);run(['createdb','restore_target_test'],target);restored=target.copy();restored['PGDATABASE']='restore_target_test'
  run(['pg_restore','--exit-on-error','--dbname=restore_target_test',str(dump)],restored)
  rows=sql('SET ROLE '+reader+'; SELECT count(*) FROM fixture.items;',restored).stdout.strip().splitlines()[-1]
  write=sql('SET ROLE '+reader+"; INSERT INTO fixture.items VALUES(3,'forbidden');",restored,False)
  privilege=sql("SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication FROM pg_roles WHERE rolname='"+reader+"';",restored).stdout.strip()
  if rows!='2' or write.returncode==0 or privilege!='f|f|f|f|f':raise RuntimeError('restored data/permission assertion failed')
  result.update(status='PASS',restored_rows=2,reader_select=True,reader_insert_refused=True,role_privileges_restored=True,dump_sha256=hashlib.sha256(dump.read_bytes()).hexdigest(),roles_sha256=hashlib.sha256(roleSQL.encode()).hexdigest())
 finally:
  clean=True
  if started:
   try:run(['pg_ctl','-D',str(out/'pg'),'-m','fast','-w','stop'],target)
   except Exception:clean=False
  if ownDB:
   try:run(['dropdb',database])
   except Exception:clean=False
  if ownRole:
   try:sql('DROP ROLE '+reader+';')
   except Exception:clean=False
  if clean:shutil.rmtree(socketDir)
  result['cleanup']=clean;(out/'report.json').write_text(json.dumps(result,indent=2));os.chmod(out/'report.json',0o600)
 print(json.dumps(result))
 if result['status']!='PASS' or not result['cleanup']:raise SystemExit(1)
if __name__=='__main__':main()
