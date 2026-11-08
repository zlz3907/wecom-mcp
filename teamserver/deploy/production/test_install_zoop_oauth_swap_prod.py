import contextlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

p=Path(__file__).with_name('install_zoop_oauth_swap_prod.py');s=importlib.util.spec_from_file_location('installer',p);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)

class InstallerTests(unittest.TestCase):
    def test_install_is_exclusive_and_can_resume_identical_files(self):
        with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
            base=Path(tmp).resolve();source=base/'source';dest=base/'dest';root=base/'root';mcp=base/'mcp';main=base/'main';approvals=base/'approvals'
            for x in (source,dest,root,mcp,main,approvals,root/'used'):x.mkdir()
            for x in (root/'release.lock',mcp/'release.lock',main/'release.lock'):x.touch()
            for src in m.FILES:(source/src).write_bytes(src.encode())
            for name,value in {'SOURCE':source,'DEST':dest,'ROOT':root,'MCP':mcp,'MAIN':main,'APPROVALS':approvals}.items():stack.enter_context(patch.object(m,name,value))
            stack.enter_context(patch.object(m.os,'geteuid',return_value=0));stack.enter_context(patch.object(m,'trusted'))
            stack.enter_context(patch.object(Path,'is_relative_to',return_value=True));stack.enter_context(patch.object(m,'baseline',return_value={'stable':True}))
            stack.enter_context(patch.object(m,'check_receipt',side_effect=lambda aid,before:{'component_sha256':m.component_hashes()}))
            m.install('APR-20261108T150000Z-test001')
            for src,target in m.FILES.items():self.assertEqual((dest/target).read_bytes(),src.encode())
            m.install('APR-20261108T150000Z-test002')
            target=dest/next(iter(m.FILES.values()));target.chmod(0o755);target.write_bytes(b'drift')
            with self.assertRaisesRegex(ValueError,'different controller'):m.install('APR-20261108T150000Z-test003')
            self.assertFalse((root/'used'/'APR-20261108T150000Z-test003').exists())

if __name__=='__main__':unittest.main()
